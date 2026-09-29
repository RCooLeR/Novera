# Encoding Fixtures

These on-disk fixtures are used by `internal/replace` tests to validate
encoding and line-ending transforms against real files (not only in-memory
buffers).

- `utf8_bom_mixed.txt`: UTF-8 with BOM and mixed CRLF/CR/LF endings.
- `utf16be_bom_mixed.txt`: UTF-16BE with BOM and mixed CRLF/CR/LF endings.
- `windows1251_crlf.txt`: Windows-1251 Cyrillic text with CRLF endings.
- `windows1252_mixed.txt`: Windows-1252 Latin text with mixed CRLF/CR/LF endings.
