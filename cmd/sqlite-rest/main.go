package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

type Config struct {
	DBPath   string
	Port     int
	Host     string
	Mode     string
	Username string
	Password string
}

type TableInfo struct {
	Name    string       `json:"name"`
	Columns []ColumnInfo `json:"columns"`
}

type ColumnInfo struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	NotNull    bool   `json:"not_null"`
	PrimaryKey bool   `json:"primary_key"`
}

type APIServer struct {
	db     *sql.DB
	router *gin.Engine
	config *Config
	tables []TableInfo
	server *http.Server
}

func parseConfig() (*Config, error) {
	config := &Config{}

	flag.StringVar(&config.DBPath, "db", "", "Path to SQLite database file (required)")
	flag.IntVar(&config.Port, "port", 8080, "Port to run the server on")
	flag.StringVar(&config.Host, "host", "0.0.0.0", "Host address to bind to")
	flag.StringVar(&config.Mode, "mode", "release", "Server mode (debug/release)")
	flag.StringVar(&config.Username, "username", "", "Basic auth username")
	flag.StringVar(&config.Password, "password", "", "Basic auth password")

	flag.Parse()

	if envUsername := os.Getenv("SQLITE_REST_USERNAME"); envUsername != "" {
		config.Username = envUsername
	}
	if envPassword := os.Getenv("SQLITE_REST_PASSWORD"); envPassword != "" {
		config.Password = envPassword
	}

	if err := validateConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

func validateConfig(config *Config) error {
	if config.DBPath == "" {
		return fmt.Errorf("database path is required")
	}

	if _, err := os.Stat(config.DBPath); os.IsNotExist(err) {
		return fmt.Errorf("database file does not exist: %s", config.DBPath)
	}

	return nil
}

func configureDatabase(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA cache_size = -2097152",    // 2GB cache
		"PRAGMA page_size = 32768",        // 32KB pages
		"PRAGMA journal_mode = OFF",       // Disable journaling
		"PRAGMA synchronous = OFF",        // Disable sync
		"PRAGMA temp_store = MEMORY",      // Memory temp tables
		"PRAGMA mmap_size = 137438953472", // 128GB mmap
		"PRAGMA query_only = 1",           // Force read-only
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to execute %q: %v", pragma, err)
		}
	}
	return nil
}

func (s *APIServer) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip auth if no credentials are configured
		if s.config.Username == "" && s.config.Password == "" {
			c.Next()
			return
		}

		username, password, hasAuth := c.Request.BasicAuth()

		if !hasAuth || username != s.config.Username || password != s.config.Password {
			c.Header("WWW-Authenticate", "Basic realm=Authorization Required")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		c.Next()
	}
}

func NewAPIServer(config *Config) (*APIServer, error) {
	gin.SetMode(config.Mode)

	db, err := sql.Open("sqlite3", config.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("database ping failed: %v", err)
	}

	if err := configureDatabase(db); err != nil {
		return nil, err
	}

	server := &APIServer{
		db:     db,
		router: gin.Default(),
		config: config,
	}

	if err := server.loadTableInfo(); err != nil {
		return nil, fmt.Errorf("failed to load table info: %v", err)
	}

	server.setupRoutes()

	return server, nil
}
func (s *APIServer) loadTableInfo() error {
	rows, err := s.db.Query(`
		SELECT name FROM sqlite_master
		WHERE type='table' AND name NOT LIKE 'sqlite_%'
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			return err
		}

		pragmaRows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
		if err != nil {
			return err
		}

		var columns []ColumnInfo
		for pragmaRows.Next() {
			var cid int
			var name, type_ string
			var notNull, pk int
			var dfltValue interface{}
			if err := pragmaRows.Scan(&cid, &name, &type_, &notNull, &dfltValue, &pk); err != nil {
				pragmaRows.Close()
				return err
			}
			columns = append(columns, ColumnInfo{
				Name:       name,
				Type:       type_,
				NotNull:    notNull == 1,
				PrimaryKey: pk == 1,
			})
		}
		pragmaRows.Close()

		s.tables = append(s.tables, TableInfo{
			Name:    tableName,
			Columns: columns,
		})
	}

	return nil
}

func (s *APIServer) setupRoutes() {
	s.router.Use(s.authMiddleware())

	s.router.GET("/", s.handleAPIInfo)

	for _, table := range s.tables {
		group := s.router.Group("/" + table.Name)
		{
			group.GET("", s.handleTableQuery(table.Name))
			group.GET("/:id", s.handleRecordQuery(table.Name))
		}
	}
}

func (s *APIServer) handleAPIInfo(c *gin.Context) {
	info := gin.H{
		"tables":    s.tables,
		"endpoints": make([]string, 0),
	}

	for _, table := range s.tables {
		info["endpoints"] = append(info["endpoints"].([]string),
			fmt.Sprintf("GET /%s", table.Name),
			fmt.Sprintf("GET /%s/:id", table.Name),
		)
	}

	c.JSON(http.StatusOK, info)
}

func (s *APIServer) handleTableQuery(tableName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get pagination parameters with defaults
		limit := 100
		offset := 0
		if limitStr := c.Query("limit"); limitStr != "" {
			if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
				limit = l
			}
		}
		if offsetStr := c.Query("offset"); offsetStr != "" {
			if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
				offset = o
			}
		}

		// Get total count
		var total int
		countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)
		if err := s.db.QueryRow(countQuery).Scan(&total); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Build query
		query := fmt.Sprintf("SELECT * FROM %s", tableName)
		var params []interface{}

		// Handle filters
		whereConditions := []string{}
		for key, values := range c.Request.URL.Query() {
			if key != "limit" && key != "offset" && key != "order" {
				whereConditions = append(whereConditions, fmt.Sprintf("%s = ?", key))
				params = append(params, values[0])
			}
		}

		if len(whereConditions) > 0 {
			query += " WHERE " + strings.Join(whereConditions, " AND ")
		}

		// Handle ordering
		if order := c.Query("order"); order != "" {
			query += fmt.Sprintf(" ORDER BY %s", order)
		}

		// Add pagination
		query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)

		// Execute query
		rows, err := s.db.Query(query, params...)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer rows.Close()

		// Process results
		columns, err := rows.Columns()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		var results []map[string]interface{}
		for rows.Next() {
			values := make([]interface{}, len(columns))
			valuePtrs := make([]interface{}, len(columns))
			for i := range values {
				valuePtrs[i] = &values[i]
			}

			if err := rows.Scan(valuePtrs...); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			row := make(map[string]interface{})
			for i, col := range columns {
				val := values[i]
				if b, ok := val.([]byte); ok {
					row[col] = string(b)
				} else {
					row[col] = val
				}
			}
			results = append(results, row)
		}

		// Return paginated response
		c.JSON(http.StatusOK, gin.H{
			"total":  total,
			"offset": offset,
			"limit":  limit,
			"data":   results,
		})
	}
}

func (s *APIServer) handleRecordQuery(tableName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		query := fmt.Sprintf("SELECT * FROM %s WHERE id = ?", tableName)
		rows, err := s.db.Query(query, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer rows.Close()

		columns, err := rows.Columns()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if !rows.Next() {
			c.JSON(http.StatusNotFound, gin.H{"error": "Record not found"})
			return
		}

		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		result := make(map[string]interface{})
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				result[col] = string(b)
			} else {
				result[col] = val
			}
		}

		c.JSON(http.StatusOK, result)
	}
}

func (s *APIServer) logStartup() {
	log.Printf("Server Configuration:")
	log.Printf("Mode: %s", s.config.Mode)
	log.Printf("Address: %s:%d", s.config.Host, s.config.Port)
	log.Printf("Database: %s", s.config.DBPath)
	log.Printf("Auth Enabled: %v", s.config.Username != "" || s.config.Password != "")

	pragmas := []string{
		"cache_size", "page_size", "journal_mode", "synchronous",
		"temp_store", "read_uncommitted", "cache_shared", "mmap_size",
	}

	log.Printf("SQLite Configuration:")
	for _, pragma := range pragmas {
		var value string
		row := s.db.QueryRow(fmt.Sprintf("PRAGMA %s", pragma))
		row.Scan(&value)
		log.Printf("  %s = %s", pragma, value)
	}
}

func (s *APIServer) Serve() error {
	s.logStartup()
	address := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)

	s.server = &http.Server{
		Addr:    address,
		Handler: s.router,
	}

	return s.server.ListenAndServe()
}

func (s *APIServer) Shutdown(ctx context.Context) error {
	if err := s.server.Shutdown(ctx); err != nil {
		return err
	}

	return s.db.Close()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func main() {
	config, err := parseConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	server, err := NewAPIServer(config)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := server.Serve(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-quit
	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}

	log.Println("Server exited")
}
