# Usage Guide

ChainLink provides a suite of commands to manage your project's dependencies and environment.

## Project Lifecycle

### Initializing a Project
To start a new project with ChainLink:
```bash
chainlink init
```
This creates a `chain.toml` file and sets up a basic project structure:
- `client/`: Client-side scripts.
- `server/`: Server-side scripts.
- `shared/`: Shared modules.
It also initializes a Git repository if one doesn't already exist.

### Installing Dependencies
To install all tools and packages listed in your `chain.toml`:
```bash
chainlink install
```

## Managing Dependencies

### Adding Tools and Packages
You can add dependencies using the `add` command.

**Adding a Tool:**
```bash
chainlink add owner/repo@tag --tool
# Example: chainlink add Kampfkarren/selene@0.32.0 --tool
```

**Adding a Package:**
```bash
chainlink add scope/name@version --package
# Example: chainlink add vibeonline/razorinput@0.1.0 --package
```

### Removing Dependencies
To remove a tool or package:
```bash
chainlink remove name
```

## Advanced Commands

### Running Tools Directly
If you don't want to create symlinks, you can run a tool directly via its spec:
```bash
chainlink run owner/repo@tag [args]
```

### The Roblox Studio Plugin
To install the ChainLink plugin for Roblox Studio:
```bash
chainlink plugin
```

### Serving
Start the ChainLink server for integrated development:
```bash
chainlink serve
```
