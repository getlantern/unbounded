<?php
/**
 * Plugin Name: Unbounded
 * Description: Unbounded widget to selected pages with editable theme, layout and location.
 * Version: 1.1
 * Author: Lantern
 */

// hooks for the admin ui
add_action('admin_menu', 'browsers_unbounded_plugin_menu');
add_action('admin_init', 'browsers_unbounded_register_settings');

// hooks for the public ui
function browsers_unbounded_add_hooks_based_on_location() {
    $options = get_option('browsers_unbounded_options');
    $location = isset($options['location']) ? $options['location'] : 'header'; // default to header

    if ($location === 'header') {
        add_action('wp_head', 'browsers_unbounded_add_element_and_script');
    } else {
        add_action('wp_footer', 'browsers_unbounded_add_element_and_script');
    }
}
add_action('init', 'browsers_unbounded_add_hooks_based_on_location');
add_action('add_meta_boxes', 'browsers_unbounded_add_meta_box');
add_action('save_post', 'browsers_unbounded_save_meta_box_data');

 // menu item for settings page
function browsers_unbounded_plugin_menu() {
    add_menu_page('Unbounded Settings', 'Unbounded', 'manage_options', 'browsers-unbounded-settings', 'browsers_unbounded_plugin_settings_page');
}

// settings for storing plugin options
function browsers_unbounded_register_settings() {
    register_setting('browsers_unbounded_options_group', 'browsers_unbounded_options', 'browsers_unbounded_options_sanitize');
    add_settings_section('browsers_unbounded_main_section', null, null, 'browsers-unbounded-settings');
    add_settings_field('browsers_unbounded_layout', 'Layout', 'browsers_unbounded_layout_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
    add_settings_field('browsers_unbounded_theme', 'Theme', 'browsers_unbounded_theme_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
    add_settings_field('browsers_unbounded_location', 'Location', 'browsers_unbounded_location_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
    add_settings_field('browsers_unbounded_homepage', 'Display on Homepage', 'browsers_unbounded_homepage_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
    add_settings_field('browsers_unbounded_posts', 'Display on Posts', 'browsers_unbounded_posts_callback', 'browsers-unbounded-settings', 'browsers_unbounded_main_section');
}

// sanitizes plugin options before saving
function browsers_unbounded_options_sanitize($options) {
    // @todo: add sanitization
    return $options;
}

// render settings page
function browsers_unbounded_plugin_settings_page() {
    ?>
    <div class="wrap">
        <h2>Unbounded Settings</h2>
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
    $options = get_option('browsers_unbounded_options');
    $layout = isset($options['layout']) ? $options['layout'] : 'banner'; // default banner
    ?>
    <select id='browsers_unbounded_layout' name='browsers_unbounded_options[layout]'>
        <option value='banner' <?php selected($layout, 'banner'); ?>>Banner</option>
        <option value='panel' <?php selected($layout, 'panel'); ?>>Panel</option>
        <option value='floating' <?php selected($layout, 'floating'); ?>>Floating</option>
    </select>
    <p class="description">Select Unbounded layout.</p>
    <?php
}


function browsers_unbounded_theme_callback() {
    $options = get_option('browsers_unbounded_options');
    $theme = isset($options['theme']) ? $options['theme'] : 'dark'; // default dark
    ?>
    <select id='browsers_unbounded_theme' name='browsers_unbounded_options[theme]'>
        <option value='light' <?php selected($theme, 'light'); ?>>Light</option>
        <option value='dark' <?php selected($theme, 'dark'); ?>>Dark</option>
        <option value='auto' <?php selected($theme, 'auto'); ?>>Auto</option>
    </select>
    <p class="description">Select Unbounded theme.</p>
    <?php
}

function browsers_unbounded_location_callback() {
    $options = get_option('browsers_unbounded_options');
    $location = isset($options['location']) ? $options['location'] : 'header'; // default header
    ?>
    <select id='browsers_unbounded_location' name='browsers_unbounded_options[location]'>
        <option value='header' <?php selected($location, 'header'); ?>>Header</option>
        <option value='footer' <?php selected($location, 'footer'); ?>>Footer</option>
    </select>
    <p class="description">Select where to add Unbounded.</p>
    <?php
}

function browsers_unbounded_homepage_callback() {
    $options = get_option('browsers_unbounded_options');
    $homepage = isset($options['homepage']) ? $options['homepage'] : ''; // default off
    ?>
    <input type='checkbox' id='browsers_unbounded_homepage' name='browsers_unbounded_options[homepage]' <?php checked($homepage, 'on'); ?> />
    <label for='browsers_unbounded_homepage'>Enable widget on the homepage</label>
    <?php
}

function browsers_unbounded_posts_callback() {
    $options = get_option('browsers_unbounded_options');
    $posts = isset($options['posts']) ? $options['posts'] : ''; // default off
    ?>
    <input type='checkbox' id='browsers_unbounded_posts' name='browsers_unbounded_options[posts]' <?php checked($posts, 'on'); ?> />
    <label for='browsers_unbounded_posts'>Enable widget on all posts</label>
    <?php
}


// meta box to the page editor to enable the unbounded widget
function browsers_unbounded_add_meta_box() {
    add_meta_box('browsers-unbounded-enable', 'Enable Unbounded', 'browsers_unbounded_meta_box_callback', 'page', 'side');
}

// renders meta box in the page editor
function browsers_unbounded_meta_box_callback($post) {
    wp_nonce_field('browsers_unbounded_meta_box', 'browsers_unbounded_meta_box_nonce');
    $value = get_post_meta($post->ID, '_browsers_unbounded_enable', true);
    echo '<label for="browsers_unbounded_field">';
    echo '<input type="checkbox" id="browsers_unbounded_field" name="browsers_unbounded_field" value="1"' . checked($value, 1, false) . ' />';
    echo ' Enable Unbounded on this page';
    echo '</label> ';
}

// saves state meta box checkbox
function browsers_unbounded_save_meta_box_data($post_id) {
    if (!isset($_POST['browsers_unbounded_meta_box_nonce']) || !wp_verify_nonce($_POST['browsers_unbounded_meta_box_nonce'], 'browsers_unbounded_meta_box')) {
        return;
    }
    if (!current_user_can('edit_post', $post_id)) {
        return;
    }
    $is_enabled = isset($_POST['browsers_unbounded_field']) ? '1' : '';
    update_post_meta($post_id, '_browsers_unbounded_enable', $is_enabled);
}

// enqueues the script and element if the page has the widget enabled
function browsers_unbounded_add_element_and_script() {
    global $post;
    $options = get_option('browsers_unbounded_options');
    $is_enabled = is_page() ? get_post_meta($post->ID, '_browsers_unbounded_enable', true) : false;
    $display_on_homepage = isset($options['homepage']) && $options['homepage'] === 'on';
    $display_on_posts = isset($options['posts']) && $options['posts'] === 'on';

    if ($is_enabled || (is_single() && $display_on_posts) || ($display_on_homepage && (is_front_page() || is_home()))) {
        // script
        echo '<script defer="defer" src="https://embed.lantern.io/static/js/main.js"></script>';

        // element — tag name is the widget's public API, so it stays "browsers-unbounded"
        $layout = isset($options['layout']) ? $options['layout'] : 'banner';
        $theme = isset($options['theme']) ? $options['theme'] : 'dark';
        printf(
            "<browsers-unbounded data-layout='%s' data-theme='%s' style='width: 100%%;'></browsers-unbounded>",
            esc_attr($layout),
            esc_attr($theme)
        );
    }
}