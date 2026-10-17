# paprika-3-mcp

A [Model Context Protocol (MCP)](https://modelcontextprotocol.io/introduction) server that exposes your **Paprika 3** recipes as LLM-readable resources — and lets an LLM like Claude create or update recipes in your Paprika app.

### 🖼️ Example: Claude using the Paprika MCP server

<p align="center">
  <img src="docs/example.png" alt="MCP server running with Claude" />
</p>

## 🚀 Features

See anything missing? Open an issue on this repo to request a feature!

#### 📄 **Resources**

- Recipes ✅
- Recipe Photos 🚧

#### 🛠 **Tools**

**Recipes**

- `search_recipes`  
  Search by name (`query`), `ingredient` and/or `category`. All given filters must match, and a category includes its subcategories. Returns one compact line per recipe: name, categories and uid.
- `get_recipe`  
  One recipe in full, by uid or name, including ingredients, directions and category names
- `list_categories`  
  All categories as a tree, with the number of recipes directly in each category and including its subcategories
- `create_paprika_recipe`  
  Save a new recipe. Pass `categories` as names of existing categories; unknown names are rejected with suggestions and the recipe is not saved. **Categories are never created.**
- `update_paprika_recipe`  
  Modify an existing recipe. Only the fields you pass change; categories, photo, rating and the created date are kept.

**Meal planner**

- `list_meals`  
  Meals between `start_date` and `end_date` (`YYYY-MM-DD`, inclusive, past or future), grouped per day with meal type and recipe name, or the free text for meals without a recipe
- `add_meal`  
  Put a recipe (`recipe`: uid or name) or free text (`name`) on a `date` with a `meal_type`. Meal types are matched against the ones defined in your Paprika account, including custom ones, and accept Dutch and English names (`ontbijt`/`breakfast`, `diner`/`dinner`, `nagerecht`/`dessert`, ...). A recipe name that matches several recipes is rejected with the candidates.

Every write triggers a sync notification so the change shows up on your other devices. There are no delete tools, and grocery lists are not supported.

#### ⚡ Caching

Recipes and categories are cached in memory and in `~/Library/Caches/paprika-3-mcp/` (recipe data only, never credentials). A refresh only downloads recipes whose hash changed, so after the first load the cache stays in sync cheaply: in the background every 5 minutes, and before a tool reads it when it is more than a minute old. Downloads are limited to 3 at a time and a refresh stops early when the API starts failing, since paprikaapp.com throttles clients that send too many requests.

## ⚙️ Prerequisites

- ✅ A Mac, Linux, or Windows system
- ✅ [Paprika 3](https://www.paprikaapp.com/) installed with cloud sync enabled
- ✅ Your Paprika 3 **username and password**
- ✅ Claude or any LLM client with **MCP tool support** enabled

## 🛠 Installation

You can download a prebuilt binary from the [Releases](https://github.com/soggycactus/paprika-3-mcp/releases) page.

### 🍎 macOS (via Homebrew)

If you're on macOS, the easiest way to install is with [Homebrew](https://brew.sh/):

```bash
brew tap soggycactus/tap
brew install paprika-3-mcp
```

### 🐧 Linux / 🪟 Windows

1. Go to the [latest release](https://github.com/soggycactus/paprika-3-mcp/releases).
2. Download the appropriate archive for your operating system and architecture:
   - `paprika-3-mcp_<version>_linux_amd64.zip` for Linux
   - `paprika-3-mcp_<version>_windows_amd64.zip` for Windows
3. Extract the zip archive:
   - **Linux**:
     ```bash
     unzip paprika-3-mcp_<version>_<os>_<arch>.zip
     ```
   - **Windows**:
     - Right-click the `.zip` file and select **Extract All**, or use a tool like 7-Zip.
4. Move the binary to a directory in your system's `$PATH`:

   - Linux:

     ```bash
     sudo mv paprika-3-mcp /usr/local/bin/
     ```

   - Windows:
     - Move `paprika-3-mcp.exe` to any folder in your `PATH` (e.g., `%USERPROFILE%\bin`)

### ✅ Test the installation

You can verify the server is installed by checking:

```bash
paprika-3-mcp --version
```

You should see:

```bash
paprika-3-mcp version v0.1.0
```

## 🤖 Setting up Claude

If you haven't setup MCP before, [first read more about how to install Claude Desktop client & configure an MCP server.](https://modelcontextprotocol.io/quickstart/user)

To add `paprika-3-mcp` to Claude, all you need to do is create another entry in the `mcpServers` section of your `claude_desktop_config.json` file:

```json
{
  "mcpServers": {
    "paprika-3": {
      "command": "paprika-3-mcp",
      "args": [
        "--username",
        "<your paprika 3 username (usually email)>",
        "--password",
        "<your paprika 3 password>"
      ]
    }
  }
}
```

Restart Claude and you should see the MCP server tools after clicking on the hammerhead icon:

![MCP server running with Claude](docs/install.png)

## 🙏 Acknowledgements

This fork builds on fixes and research from the community: login encoding (#8), empty categories (#12), and the bulk meal endpoint and retry approach from [JoshTerAvest](https://github.com/JoshTerAvest/paprika-3-mcp) (#11). Meal types via `/sync/mealtypes` were inspired by [brendanjerwin's fork](https://github.com/brendanjerwin/paprika-3-mcp).

## 📄 License

This project is open source under the [MIT License](./LICENSE) © 2025 [Lucas Stephens](https://github.com/soggycactus).

---

#### 🗂 Miscellaneous

##### 📄 Where can I see the server logs?

The MCP server writes structured logs using Go’s `slog` with rotation via `lumberjack`. Log files are automatically created based on your operating system:

| Operating System | Log File Path                             |
| ---------------- | ----------------------------------------- |
| macOS            | `~/Library/Logs/paprika-3-mcp/server.log` |
| Linux            | `/var/log/paprika-3-mcp/server.log`       |
| Windows          | `%APPDATA%\paprika-3-mcp\server.log`      |
| Other / Unknown  | `/tmp/paprika-3-mcp/server.log`           |

> 💡 Logs are rotated automatically at 100MB, with only 5 backup files kept. Logs are also wiped after 10 days.
