# SQLite REST API Server

A lightweight REST API server that provides HTTP access to SQLite databases.

> ⚠️ **Disclaimer**: This project is in a very early development stage and should not be used in production environments.

## Features

- RESTful API for querying SQLite databases
- Read-only access with query-only mode enforced
- Basic authentication support
- Pagination and filtering support
- Memory-optimized SQLite configuration
- Automatic table schema detection

## Installation

## Usage

Start the server by providing a path to your SQLite database:

### Command Line Options

- `-db` - Path to SQLite database file (required)
- `-port` - Port to run the server on (default: 8080)
- `-host` - Host address to bind to (default: 0.0.0.0)
- `-mode` - Server mode (debug/release) (default: release)
- `-username` - Basic auth username
- `-password` - Basic auth password

### Environment Variables

- `SQLITE_REST_USERNAME` - Basic auth username
- `SQLITE_REST_PASSWORD` - Basic auth password

### API Endpoints

- `GET /` - List available tables and their schemas
- `GET /<table>` - Query table with pagination and filters
- `GET /<table>/<id>` - Get single record by ID

### Query Parameters

- `limit` - Number of records to return (default: 100)
- `offset` - Number of records to skip
- `order` - Column to sort by
- Any other parameter will be used as a filter condition

## Current Limitations

- Read-only access
- No schema modifications
- No complex queries
- Basic authentication only
