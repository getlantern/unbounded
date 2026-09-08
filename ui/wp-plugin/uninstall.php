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

delete_option('browsers_unbounded_options');

// Set per page via the editor meta box, so it can be on any number of posts.
delete_post_meta_by_key('_browsers_unbounded_enable');
