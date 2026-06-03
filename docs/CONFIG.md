# Configuration File Documentation

This document explains how to use JSON configuration files with sonar-migration-tool instead of command-line arguments.

## Why Use Configuration Files?

Configuration files offer several advantages:

- **Security**: Keep sensitive tokens in a file that's not in version control
- **Convenience**: Avoid typing long command-line arguments repeatedly
- **Reproducibility**: Share configurations easily across teams
- **Clarity**: All settings in one place, easy to review and modify

## Quick Start

1. Copy an example config file:
   ```bash
   cp config-extract.example.json my-config.json
   ```

2. Edit the file with your values:
   ```json
   {
     "url": "http://localhost:9000",
     "token": "squ_your_actual_token_here",
     "export_directory": "./files",
     "concurrency": 10,
     "timeout": 60
   }
   ```

3. Use it with the executable:
   ```bash
   ./sonar-migration-tool extract --config my-config.json
   ```

## Configuration File Format

Configuration files are standard JSON format. All fields are optional - you can specify only the values you need, and use command-line arguments for the rest.

### For Extract Command

Create a file named `extract-config.json`:

```json
{
  "url": "http://localhost:9000",
  "token": "YOUR_SONARQUBE_TOKEN",
  "export_directory": "./files",
  "extract_type": "all",
  "concurrency": 10,
  "timeout": 60,
  "pem_file_path": null,
  "key_file_path": null,
  "cert_password": null,
  "target_task": null,
  "extract_id": null
}
```

Usage:
```bash
./sonar-migration-tool extract --config extract-config.json
```

### For Migrate Command

Create a file named `migrate-config.json`:

```json
{
  "token": "YOUR_SONARCLOUD_TOKEN",
  "enterprise_key": "YOUR_ENTERPRISE_KEY",
  "url": "https://sonarcloud.io/",
  "edition": "enterprise",
  "export_directory": "./files",
  "concurrency": 10,
  "run_id": null,
  "target_task": null
}
```

Usage:
```bash
./sonar-migration-tool migrate --config migrate-config.json
```

## Configuration Parameters

### Extract Command Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `url` | string | **required** | SonarQube server URL |
| `token` | string | **required** | Admin user token for SonarQube |
| `export_directory` | string | `/app/files/` | Directory to output export files |
| `extract_type` | string | `all` | Type of extract to run |
| `concurrency` | number | `25` | Maximum concurrent requests |
| `timeout` | number | `60` | Request timeout in seconds |
| `pem_file_path` | string | `null` | Path to client certificate PEM file |
| `key_file_path` | string | `null` | Path to client certificate key file |
| `cert_password` | string | `null` | Password for client certificate |
| `target_task` | string | `null` | Target task to complete |
| `extract_id` | string | `null` | ID of extract to resume |

### Migrate Command Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `token` | string | **required** | SonarCloud admin token |
| `enterprise_key` | string | **required** | SonarCloud enterprise key |
| `url` | string | `https://sonarcloud.io/` | SonarCloud URL |
| `edition` | string | `enterprise` | SonarCloud license edition |
| `export_directory` | string | `/app/files/` | Directory containing exports |
| `concurrency` | number | `25` | Maximum concurrent requests |
| `run_id` | string | `null` | ID of run to resume |
| `target_task` | string | `null` | Specific migration task |

### Structure and Mappings Commands

Both `structure` and `mappings` accept `--config` pointing at the same JSON
file used by `extract`. They read **only `export_directory`** from it — every
other field is ignored. `structure` additionally pre-populates the
`sonarcloud_org_key` column in `organizations.csv` when exactly one
SonarCloud organization is defined in the config (under `target.organizations`
in the unified shape, or `organizations` in the migrate shape).

```bash
# Reuse the extract config — export_directory comes from the JSON
./sonar-migration-tool structure --config extract-config.json
./sonar-migration-tool mappings  --config extract-config.json
```

Precedence (same as `extract`, `migrate`, and `predictive-report`):

1. `--export_directory` on the CLI wins when explicitly set.
2. Otherwise `export_directory` from `--config` is used.
3. Otherwise the default `./migration-files` is used.

## Combining Config Files and CLI Arguments

Command-line arguments **override** config file values. This allows you to:

- Keep common settings in a config file
- Override specific values on the command line

Example:
```bash
# Config file has concurrency: 10
./sonar-migration-tool extract --config my-config.json --concurrency 5

# This will use concurrency of 5, overriding the config file
```

## Security Best Practices

### 1. Never Commit Tokens to Version Control

Add your config files to `.gitignore`:

```bash
echo "config.json" >> .gitignore
echo "*-config.json" >> .gitignore
echo "my-*.json" >> .gitignore
```

### 2. Use Environment Variables

You can reference environment variables in your workflow:

```bash
# Set token as environment variable
export SONAR_TOKEN="squ_your_token"

# Create config file dynamically
cat > config.json <<EOF
{
  "url": "http://localhost:9000",
  "token": "$SONAR_TOKEN",
  "export_directory": "./files"
}
EOF

# Run the tool
./sonar-migration-tool extract --config config.json

# Clean up
rm config.json
```

### 3. Use File Permissions

Restrict access to config files containing sensitive data:

```bash
chmod 600 my-config.json
```

This ensures only you can read/write the file.

## Example Workflows

### Development Environment

Create `dev-config.json`:
```json
{
  "url": "http://localhost:9000",
  "token": "LOCAL_DEV_TOKEN",
  "export_directory": "./dev-files",
  "concurrency": 5
}
```

### Production Environment

Create `prod-config.json`:
```json
{
  "url": "https://sonarqube.company.com",
  "token": "PROD_TOKEN_HERE",
  "export_directory": "/data/sonar-exports",
  "concurrency": 20,
  "timeout": 120
}
```

### Full Migration Workflow

1. Extract config (`extract-config.json`):
```json
{
  "url": "http://sonarqube-server:9000",
  "token": "SONARQUBE_TOKEN",
  "export_directory": "./migration-data"
}
```

2. Migrate config (`migrate-config.json`):
```json
{
  "token": "SONARCLOUD_TOKEN",
  "enterprise_key": "my-enterprise",
  "url": "https://sonarcloud.io/",
  "export_directory": "./migration-data"
}
```

3. Run the workflow:
```bash
# Extract from SonarQube
./sonar-migration-tool extract --config extract-config.json

# Generate structure
./sonar-migration-tool structure --config extract-config.json

# Generate mappings
./sonar-migration-tool mappings --config extract-config.json

# Edit organizations.csv to add SonarCloud org keys
# Then migrate
./sonar-migration-tool migrate --config migrate-config.json
```

## Troubleshooting

### "Error loading config file: ..."

- Check that the file path is correct
- Ensure the JSON syntax is valid (no trailing commas, proper quotes)
- Verify the file is readable

### "Error: URL and TOKEN are required"

- Make sure these fields are in your config file OR provided as CLI arguments
- Check for typos in the field names

### Values Not Being Applied

- Remember: CLI arguments override config file values
- Check that you're using the correct config file path
- Verify JSON syntax is valid

## Example Config Files

The repository includes these example files:

- `config.example.json` - Complete example with all options
- `config-extract.example.json` - Minimal extract config
- `config-migrate.example.json` - Minimal migrate config

Copy and customize these for your needs.
