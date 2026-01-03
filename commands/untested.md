---
description: Find code without test coverage
---

# Find Untested Code

Search the codebase for exported functions that are not called from any test files.

Use semantic search to find code that:
1. Is exported (public API)
2. Has the "not_tested" flag set to true
3. Is not itself test code

Present the results prioritized by:
1. Complexity/importance of the function
2. Whether it handles errors or edge cases
3. Public API surface area

For each result, show:
- Function name and signature
- File location
- What it does
- Suggested test cases to add

Focus on "$ARGUMENTS" if specified, otherwise scan the entire indexed codebase.
