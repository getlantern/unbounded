<?php
/**
 * Plugin Name:       Unbounded
 * Plugin URI:        https://unbounded.lantern.io
 * Description:       Let visitors volunteer a slice of their connection to help people reach the open internet. Enable the widget per page, on all posts, or on the homepage.
 * Version:           1.2.0
 * Requires at least: 5.0
 * Requires PHP:      7.4
 * Author:            Lantern
 * Author URI:        https://lantern.io
 * License:           GPL-3.0-or-later
 * License URI:       https://www.gnu.org/licenses/gpl-3.0.html
 * Text Domain:       unbounded
 */

// The plugin is a thin client for the Unbounded network: it prints the widget
// element and loads the widget bundle, and everything else -- peer discovery,
// transport, the WebAssembly proxy -- happens in the service. Nothing here
// starts proxying; the widget does that only when a visitor flips its switch.

if (!defined('ABSPATH')) {
    exit;
}

/**
 * URL of the widget bundle.
 *
 * Filterable so a site can serve the bundle itself rather than from
 * embed.lantern.io. Self-hosting pins the widget to whatever build was copied,
 * which will eventually fall out of step with the network, so the hosted URL is
 * the default.
 *
 * @return string
 */
function browsers_unbounded_script_url() {
    return apply_filters('browsers_unbounded_script_url', 'https://embed.lantern.io/static/js/main.js');
}

/** @return array<string, string> Allowed values keyed by option, first entry is the default. */
function browsers_unbounded_allowed_values() {
    return array(
        'layout'   => array('banner', 'panel', 'floating', 'simple'),
        'theme'    => array('dark', 'light', 'auto'),
        'location' => array('header', 'footer'),
    );
}

/** @return array<string, string> Stored options merged over the defaults. */
function browsers_unbounded_get_options() {
    $allowed = browsers_unbounded_allowed_values();
    $defaults = array(
        'layout'   => $allowed['layout'][0],
        'theme'    => $allowed['theme'][0],
        'location' => $allowed['location'][0],
        'homepage' => '',
        'posts'    => '',
    );

    $stored = get_option('browsers_unbounded_options');
    if (!is_array($stored)) {
        return $defaults;
    }

    return array_merge($defaults, $stored);
}

// hooks for the admin ui
add_action('admin_menu', 'browsers_unbounded_plugin_menu');
add_action('admin_init', 'browsers_unbounded_register_settings');

// hooks for the public ui
function browsers_unbounded_add_hooks_based_on_location() {
    $options = browsers_unbounded_get_options();

    if ($options['location'] === 'header') {
        add_action('wp_head', 'browsers_unbounded_add_element');
    } else {
        add_action('wp_footer', 'browsers_unbounded_add_element');
    }
}
add_action('init', 'browsers_unbounded_add_hooks_based_on_location');
add_action('wp_enqueue_scripts', 'browsers_unbounded_enqueue_script');
add_action('add_meta_boxes', 'browsers_unbounded_add_meta_box');
add_action('save_post', 'browsers_unbounded_save_meta_box_data');

 // menu item for settings page
function browsers_unbounded_plugin_menu() {
    add_menu_page(
        __('Unbounded Settings', 'unbounded'),
        __('Unbounded', 'unbounded'),
        'manage_options',
        'browsers-unbounded-settings',
        'browsers_unbounded_plugin_settings_page'
    );
}

// settings for storing plugin options
function browsers_unbounded_register_settings() {
    register_setting('browsers_unbounded_options_group', 'browsers_unbounded_options', array(
        'sanitize_callback' => 'browsers_unbounded_options_sanitize',
        'default'           => array(),
    ));
    add_settings_section('browsers_unbounded_main_section', null, null, 'browsers-unbounded-settings');
    add_settings_field('browsers_unbounded_layout', __('Layout', 'unbounded'), 'browsers_unbounded_layout_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
    add_settings_field('browsers_unbounded_theme', __('Theme', 'unbounded'), 'browsers_unbounded_theme_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
    add_settings_field('browsers_unbounded_location', __('Location', 'unbounded'), 'browsers_unbounded_location_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
    add_settings_field('browsers_unbounded_homepage', __('Display on Homepage', 'unbounded'), 'browsers_unbounded_homepage_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
    add_settings_field('browsers_unbounded_posts', __('Display on Posts', 'unbounded'), 'browsers_unbounded_posts_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
}

/**
 * Sanitizes plugin options before saving.
 *
 * The select values reach the front end as attributes on the widget element and
 * the checkboxes decide whether it renders at all, so anything not on the
 * allowlist falls back to the default rather than being stored and echoed.
 *
 * @param mixed $input Raw submitted value.
 * @return array<string, string>
 */
function browsers_unbounded_options_sanitize($input) {
    $allowed = browsers_unbounded_allowed_values();
    $clean = array();

    foreach ($allowed as $key => $values) {
        // is_scalar guard rather than relying on sanitize_key's own, which only
        // grew one around WP 5.6; before that an array reached strtolower().
        $submitted = (isset($input[$key]) && is_scalar($input[$key]))
            ? sanitize_key($input[$key])
            : '';
        $clean[$key] = in_array($submitted, $values, true) ? $submitted : $values[0];
    }

    foreach (array('homepage', 'posts') as $key) {
        $clean[$key] = empty($input[$key]) ? '' : 'on';
    }

    return $clean;
}

// render settings page
function browsers_unbounded_plugin_settings_page() {
    if (!current_user_can('manage_options')) {
        return;
    }
    ?>
    <div class="wrap">
        <h2><?php esc_html_e('Unbounded Settings', 'unbounded'); ?></h2>
        <form method="post" action="options.php">
            <?php settings_fields('browsers_unbounded_options_group'); ?>
            <?php do_settings_sections('browsers-unbounded-settings'); ?>
            <?php submit_button(); ?>
        </form>
    </div>
    <?php
}

// cb for rendering setting fields
function browsers_unbounded_layout_callback() {
    $layout = browsers_unbounded_get_options()['layout'];
    ?>
    <select id='browsers_unbounded_layout' name='browsers_unbounded_options[layout]'>
        <option value='banner' <?php selected($layout, 'banner'); ?>><?php esc_html_e('Banner', 'unbounded'); ?></option>
        <option value='panel' <?php selected($layout, 'panel'); ?>><?php esc_html_e('Panel', 'unbounded'); ?></option>
        <option value='floating' <?php selected($layout, 'floating'); ?>><?php esc_html_e('Floating', 'unbounded'); ?></option>
        <option value='simple' <?php selected($layout, 'simple'); ?>><?php esc_html_e('Simple', 'unbounded'); ?></option>
    </select>
    <p class="description"><?php esc_html_e('Select Unbounded layout.', 'unbounded'); ?></p>
    <?php
}


function browsers_unbounded_theme_callback() {
    $theme = browsers_unbounded_get_options()['theme'];
    ?>
    <select id='browsers_unbounded_theme' name='browsers_unbounded_options[theme]'>
        <option value='light' <?php selected($theme, 'light'); ?>><?php esc_html_e('Light', 'unbounded'); ?></option>
        <option value='dark' <?php selected($theme, 'dark'); ?>><?php esc_html_e('Dark', 'unbounded'); ?></option>
        <option value='auto' <?php selected($theme, 'auto'); ?>><?php esc_html_e('Auto', 'unbounded'); ?></option>
    </select>
    <p class="description"><?php esc_html_e('Select Unbounded theme.', 'unbounded'); ?></p>
    <?php
}

function browsers_unbounded_location_callback() {
    $location = browsers_unbounded_get_options()['location'];
    ?>
    <select id='browsers_unbounded_location' name='browsers_unbounded_options[location]'>
        <option value='header' <?php selected($location, 'header'); ?>><?php esc_html_e('Header', 'unbounded'); ?></option>
        <option value='footer' <?php selected($location, 'footer'); ?>><?php esc_html_e('Footer', 'unbounded'); ?></option>
    </select>
    <p class="description"><?php esc_html_e('Select where to add Unbounded.', 'unbounded'); ?></p>
    <?php
}

function browsers_unbounded_homepage_callback() {
    $homepage = browsers_unbounded_get_options()['homepage'];
    ?>
    <input type='checkbox' id='browsers_unbounded_homepage' name='browsers_unbounded_options[homepage]' <?php checked($homepage, 'on'); ?> />
    <label for='browsers_unbounded_homepage'><?php esc_html_e('Enable widget on the homepage', 'unbounded'); ?></label>
    <?php
}

function browsers_unbounded_posts_callback() {
    $posts = browsers_unbounded_get_options()['posts'];
    ?>
    <input type='checkbox' id='browsers_unbounded_posts' name='browsers_unbounded_options[posts]' <?php checked($posts, 'on'); ?> />
    <label for='browsers_unbounded_posts'><?php esc_html_e('Enable widget on all posts', 'unbounded'); ?></label>
    <?php
}


// meta box to the page editor to enable the unbounded widget
function browsers_unbounded_add_meta_box() {
    add_meta_box('browsers-unbounded-enable', __('Enable Unbounded', 'unbounded'), 'browsers_unbounded_meta_box_callback', 'page', 'side');
}

// renders meta box in the page editor
function browsers_unbounded_meta_box_callback($post) {
    wp_nonce_field('browsers_unbounded_meta_box', 'browsers_unbounded_meta_box_nonce');
    $value = get_post_meta($post->ID, '_browsers_unbounded_enable', true);
    ?>
    <label for="browsers_unbounded_field">
        <input type="checkbox" id="browsers_unbounded_field" name="browsers_unbounded_field" value="1" <?php checked($value, 1); ?> />
        <?php esc_html_e('Enable Unbounded on this page', 'unbounded'); ?>
    </label>
    <?php
}

// saves state meta box checkbox
function browsers_unbounded_save_meta_box_data($post_id) {
    if (defined('DOING_AUTOSAVE') && DOING_AUTOSAVE) {
        return;
    }
    if (!isset($_POST['browsers_unbounded_meta_box_nonce'])) {
        return;
    }
    $nonce = sanitize_text_field(wp_unslash($_POST['browsers_unbounded_meta_box_nonce']));
    if (!wp_verify_nonce($nonce, 'browsers_unbounded_meta_box')) {
        return;
    }
    if (!current_user_can('edit_post', $post_id)) {
        return;
    }
    $is_enabled = isset($_POST['browsers_unbounded_field']) ? '1' : '';
    update_post_meta($post_id, '_browsers_unbounded_enable', $is_enabled);
}

/**
 * Whether the widget should render on the request being served.
 *
 * @return bool
 */
function browsers_unbounded_should_display() {
    global $post;
    $options = browsers_unbounded_get_options();

    $is_enabled = (is_page() && $post) ? get_post_meta($post->ID, '_browsers_unbounded_enable', true) : false;
    $display_on_homepage = $options['homepage'] === 'on';
    $display_on_posts = $options['posts'] === 'on';

    return (bool) $is_enabled
        || (is_single() && $display_on_posts)
        || ($display_on_homepage && (is_front_page() || is_home()));
}

// enqueues the widget bundle when the widget is going to render
function browsers_unbounded_enqueue_script() {
    if (!browsers_unbounded_should_display()) {
        return;
    }

    // No version string: the bundle is served unversioned, and appending ?ver=
    // would only bust the CDN cache for every visitor.
    wp_enqueue_script(
        'browsers-unbounded-widget',
        browsers_unbounded_script_url(),
        array(),
        null,
        browsers_unbounded_get_options()['location'] === 'footer'
    );
}

// the bundle is self-contained and does not block rendering, so defer it. Set
// via filter rather than wp_enqueue_script's $args array, which needs WP 6.3.
function browsers_unbounded_defer_script($tag, $handle) {
    if ($handle !== 'browsers-unbounded-widget') {
        return $tag;
    }
    return str_replace(' src=', ' defer="defer" src=', $tag);
}
add_filter('script_loader_tag', 'browsers_unbounded_defer_script', 10, 2);

// prints the element the widget bundle mounts into
function browsers_unbounded_add_element() {
    if (!browsers_unbounded_should_display()) {
        return;
    }

    $options = browsers_unbounded_get_options();

    // The tag name is the widget's public API -- the bundle only looks for
    // "browsers-unbounded" (or the legacy "lantern-network") -- so it stays put
    // even though the product is now called Unbounded.
    printf(
        "<browsers-unbounded data-layout='%s' data-theme='%s' style='width: 100%%;'></browsers-unbounded>",
        esc_attr($options['layout']),
        esc_attr($options['theme'])
    );
}
