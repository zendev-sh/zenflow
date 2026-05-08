# Architecture

The system follows a layered architecture:

- **API Layer**: HTTP handlers using Go stdlib net/http
- **Service Layer**: Business logic with interfaces for testability
- **Storage Layer**: Database access via repository pattern
- **Config**: Environment-based configuration with defaults
