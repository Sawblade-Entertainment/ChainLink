# Tool Proxying

Tool proxying is a core feature of ChainLink that allows you to use managed tools as if they were native system binaries.

## How it Works

The `chainlink` binary is designed to detect how it was invoked. If it is called by a name other than `chainlink` (for example, if it's called via a symlink named `selene`), it enters **Proxy Mode**.

### The Proxy Flow
1. **Invocation**: You run `selene --help`.
2. **Detection**: The OS resolves the `selene` symlink to the `chainlink` binary.
3. **Resolution**: ChainLink looks at the name of the symlink (`selene`) and searches the `[Tools]` section of the local `chain.toml` for a matching entry.
4. **Execution**: Once the spec (e.g., `Kampfkarren/selene@0.32.0`) is found, ChainLink:
   - Checks if that specific version is installed in `~/.chainlink/tools/selene/0.32.0`.
   - If not installed, it downloads and installs the tool automatically.
   - Executes the binary with the original arguments (`--help`).

## Setting Up Proxying

To set up proxying for a tool, create a symlink in a directory that is in your system `PATH` (such as `/usr/local/bin` or `~/.chainlink/bin`):

```bash
ln -s $(which chainlink) /usr/local/bin/selene
```

Now, whenever you run `selene`, ChainLink will ensure the version specified in your current project's `chain.toml` is used. If you move to a different project with a different version of Selene in its `chain.toml`, the same `selene` command will automatically use that version instead.

## Benefits
- **Zero Setup for Teams**: Just add the symlink once; the tools are managed per-project.
- **Version Switching**: No need to manually update tool versions when switching branches or projects.
- **Automatic Installation**: Tools are installed on-demand the first time they are run.
