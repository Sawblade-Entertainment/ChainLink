# Macros

The build system provides a set of global "macros" (injected helper functions) that are prepended to Luau scripts if their usage is detected in the source code.

## Injected Globals

### `use "module"`
A versatile utility for loading modules or retrieving services.

**Behavior:**
- If `module` is a string:
    - If it starts with `.` or `@`, it is passed directly to `require(module)`.
    - If it is lowercase, it is treated as a package name and resolved via `game.ReplicatedStorage.packages[module]`.
    - Otherwise, it is treated as a Roblox Service name and resolved via `game:GetService(module)`.
- Otherwise, `module` is passed directly to `require(module)`.

**Example:**
```lua
local TweenService = use "TweenService" -- Retrieves game:GetService("TweenService")
local MyPackage = use "my_package"      -- Retrieves require(game.ReplicatedStorage.packages.my_package)
local MyModule = use path.to.module     -- Retrieves require(path.to.module)
```

### `new "instance"`
A shorthand for creating new Roblox instances.

**Behavior:**
- Calls `Instance.new(instance)` and returns the result.

**Example:**
```lua
local part = new "Part" -- Equivalent to Instance.new("Part")
```
