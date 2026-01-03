---
description: Find potentially unused/dead code in the codebase
---

# Find Unused Code

Search the codebase for exported functions and methods that have no callers (potential dead code).

Use semantic search to find code that:
1. Is exported (public API)
2. Has the "is_unused" flag set to true
3. Is not test code

Present the results organized by file, showing:
- Function/method name
- File location
- Brief description of what it does
- Recommendation (safe to remove, needs verification, etc.)

Focus on "$ARGUMENTS" if specified, otherwise scan the entire indexed codebase.
