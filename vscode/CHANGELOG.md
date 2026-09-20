# Changelog

## 0.1.0

- Lua debugger: breakpoints, conditional breakpoints, stepping, call stack, locals/upvalues, table expansion, evaluate, pause on runtime errors (`bin/lua-dap` + `lua/debugger.lua`).
- Lua language server: diagnostics, outline, go to definition, completion, hover, Run/Debug code lenses (`bin/lua-lsp`, tree-sitter based).
- Launch options `packagePath`, `packageCPath`, `env`; English and Simplified Chinese UI.
- Platform-specific VSIX packages built by GitHub Actions.
