---
description: Find all callers of a function (call graph analysis)
---

# Find Function Callers

Search for all functions that call "$ARGUMENTS".

Use the semantic search with usage tracking to:
1. Find the target function/method
2. Show all direct callers (level 1)
3. Show callers of callers (level 2-3) if available

Present results as a call hierarchy:
```
Target: functionName
├── DirectCaller1 (file.go:42)
│   ├── IndirectCaller1 (other.go:15)
│   └── IndirectCaller2 (test.go:88) [TEST]
└── DirectCaller2 (main.go:23)
```

Highlight:
- Test callers with [TEST] marker
- Entry points (functions with no callers)
- Potential circular dependencies
