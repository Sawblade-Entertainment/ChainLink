# Configuration

ChainLink's behavior is driven by the `chain.toml` file and a hidden authentication store.

## The `chain.toml` File

This file tracks all project-level dependencies and metadata.

### `[Package]` Section
Contains metadata about the current project. This is used when publishing the project as a package.

| Field | Type | Description |
| :--- | :--- | :--- |
| `Name` | String | The name of the package. |
| `Version` | String | The semver version of the package. |
| `License` | String | The license (e.g., "MIT"). |
| `Description` | String | A short description of the project. |
| `Website` | String | The project's homepage URL. |

### `[Tools]` Section
A map of tool names to their specifications.
**Format:** `tool_name = "owner/repo@tag"`
Example: `selene = "Kampfkarren/selene@0.32.0"`

### `[Packages]` Section
A map of package names to their specifications.
**Format:** `package_name = "scope/name@version"`
Example: `razorinput = "vibeonline/razorinput@0.1.0"`

## Authentication Store

ChainLink stores API tokens for various registries in a file located at `~/.chainlink/auth.toml`.

### How it Works
When you run `chainlink login`, ChainLink retrieves the registry's API endpoint from its index configuration. The token is then stored mapped to that API URL.

### Manual Management
The `auth.toml` file is a simple TOML file with a `[tokens]` map:
```toml
[tokens]
"https://api.wally.run" = "your-api-token-here"
```
