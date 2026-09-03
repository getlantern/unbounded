# Unbounded WordPress Plugin

Quick guide to get the Unbounded plugin running with Docker

## Setup

1. **Start Docker:**
   ```bash
   docker-compose up -d
    ```
   
2. **Create Plugin Directory:**
    ```bash
    mkdir ./wp-content/plugins/browsers-unbounded-plugin
    ```
   
3. **Copy Plugin File:**
    ```bash
   cp browsers-unbounded-plugin.php ./wp-content/plugins/browsers-unbounded-plugin/
    ```
   
4. **Activate Plugin:**
    - Go to `http://localhost:8000/wp-admin/plugins.php`
    - Find the `Unbounded` plugin and click `Activate`

5. **Test Plugin:**
    - Go to `http://localhost:8000/wp-admin/admin.php?page=browsers-unbounded-settings`
    - You should see the plugin settings page
    - Configure the plugin settings as needed
    - Create a new page and check the `Enable Unbounded` box in the `Page Attributes` section
    - View the page and you should see the widget in action