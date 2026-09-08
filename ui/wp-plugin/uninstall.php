<?php
/**
 * Removes everything the plugin stored.
 *
 * Runs only on delete, not deactivate, so settings survive a deactivate/
 * reactivate cycle but leave nothing behind once the plugin is actually gone.
 */

if (!defined('WP_UNINSTALL_PLUGIN')) {
    exit;
}

/**
 * Deletes the plugin's data for whichever site is current.
 *
 * The post meta is set per page via the editor meta box, so it can be on any
 * number of posts.
 */
function browsers_unbounded_delete_site_data() {
    delete_option('browsers_unbounded_options');
    delete_post_meta_by_key('_browsers_unbounded_enable');
}

/**
 * Both deletes above act on the current site only, and uninstall.php runs once
 * for the whole network, so on multisite every other site would keep its row.
 *
 * Wrapped in a function rather than run at file scope to keep the loop variable
 * out of the global namespace.
 */
function browsers_unbounded_uninstall() {
    if (!is_multisite()) {
        browsers_unbounded_delete_site_data();
        return;
    }

    foreach (get_sites(array('fields' => 'ids', 'number' => 0)) as $site_id) {
        switch_to_blog($site_id);
        browsers_unbounded_delete_site_data();
        restore_current_blog();
    }
}

browsers_unbounded_uninstall();
