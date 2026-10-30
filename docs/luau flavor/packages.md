# Packages

ChainLink provides a custom `use` implementation for the Luau flavor to simplify dependency management and package resolution.

## Import Syntax

All imports should be placed at the top of your script. The custom require uses a string-based pathing system:

```lua
-- Correct import format
RazorInput = use "razorinput"
CharacterController = use "./CharacterController"
```

## Path Resolution

The behavior of `use` depends on the prefix of the path provided:

### 1. Package Resolution (Non-Relative)
If a path does **not** start with a relative indicator (`./`, `../`, or `@`), the bootstrapper diverts the search to `ReplicatedStorage.packages`.

- **Example**: `use "razorinput"` > Looks for `game.ReplicatedStorage.packages.razorinput`.
- This allows you to reference shared libraries and third-party packages without knowing their exact location in the DataModel.

### 2. Relative Resolution
Paths starting with `./` or `../` resolve relative to the current script's location in the hierarchy, similar to standard file system paths.

### 3. Absolute/Special Resolution
Paths starting with `@` are handled as special absolute paths (depending on project configuration).

## Best Practices

- **Top-of-File**: Always place your `use` calls at the very beginning of the script to ensure dependencies are loaded before any logic executes.
- **Naming**: Use lowercase for package names in `ReplicatedStorage.packages` to maintain consistency across the project.
- **Service**: Use uppercase for service names. Acts as a shortcut to game:GetService(). use "Players" or game:GetService("Players") whichever you prefer.
