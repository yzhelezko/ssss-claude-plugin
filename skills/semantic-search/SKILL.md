---
name: semantic-search
description: Use this skill when searching for code by meaning/intent rather than exact text. Helps find functions, patterns, implementations, and understand code relationships. Use when user asks to find code, understand what calls what, or locate implementations.
---

# Semantic Code Search Skill

This skill uses the SSSS (Stupid Simple Semantic Search) MCP server to find code based on meaning rather than exact text matches.

## When to Use

Use this skill when the user:
- Asks to find code that does something specific (e.g., "find the authentication logic")
- Wants to understand code relationships (e.g., "what calls this function?")
- Is looking for implementations (e.g., "where is error handling done?")
- Needs to find unused or untested code
- Wants to explore the codebase structure

## How to Search

Use the `semantic_search` MCP tool with natural language queries:
- Be specific about what you're looking for
- Include context about the programming language if relevant
- Use domain terms from the codebase

## Understanding Results

Each search result includes:
- **File path and lines**: Where the code is located
- **Content**: The actual code snippet
- **Similarity score**: How well it matches (0-100%)
- **Usage info** (when available):
  - `is_exported`: Whether it's a public API
  - `is_unused`: No callers found (potential dead code)
  - `not_tested`: No test coverage
  - `calls`: Functions this code calls
  - `called_by`: Functions that call this code

## Tips

1. Start with broad queries, then narrow down
2. Use the usage info to understand code importance
3. Check `called_by` to understand impact of changes
4. Look at `calls` to understand dependencies
5. Flag `is_unused` code for potential cleanup
