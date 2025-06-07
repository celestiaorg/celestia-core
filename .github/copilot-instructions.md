# GitHub Copilot Instructions for celestia-core

## General Guidelines

Whenever you generate code or documentation:

1. Use extremely simple, direct language—no unnecessary adverbs.
2. Make the code self-explanatory. Only add comments when an implicit operation must be called out.
3. Follow instructions exactly. Think critically about security: always double-check for hidden bugs or vulnerabilities.
4. Produce readable yet concise code without premature abstraction.
5. When writing Go, adhere to the latest official Go best practices (idiomatic naming, error handling, package layout, etc.).
6. Keep suggestions minimal and focused. Avoid excessive detail or overly prescriptive guidance.
7. When debugging you must fix the root of the problem, not hack around the symptoms.
## Go-Specific Guidelines

### Function Structure
- Keep functions focused and single-purpose
- Prefer early returns to reduce nesting
- Validate inputs at the beginning of functions
- Use guard clauses to handle edge cases early

### Testing
- Follow table-driven test patterns established in the codebase
- Use `testify/assert` and `testify/require` for assertions
- Name test cases descriptively to explain what is being tested
- Include both positive and negative test cases
- Test edge cases and error conditions

## Blockchain-Specific Guidelines

### Security Considerations
- Always validate user inputs, especially in message handlers
- Be cautious with arithmetic operations that could overflow
- Verify permissions and authority before state modifications
- Consider replay attacks and ensure proper nonce/sequence handling
- Be mindful of gas consumption and potential DoS vectors

## Pull Request Rules
- When naming a PR or commits, always follow [conventional commits](https://www.conventionalcommits.org/en/v1.0.0/#summary)

## Code Organization
- Analyze the project structure entirely before deciding where something should go.
- Prefer standard library solutions when possible

### Linting
- Use golangci-lint and gofmt before submitting

### Documentation
- Document all exported functions, types, and constants
- Use godoc-style comments that start with the item name
- Keep documentation concise
- Only document mid code for non-obvious behavior or side effects