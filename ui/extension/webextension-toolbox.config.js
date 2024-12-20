const webpack = require('webpack')
const {resolve} = require("path")
const GlobEntriesPlugin = require("webpack-watched-glob-entries-plugin")
const dotenv = require('dotenv').config({ path: __dirname + '/.env' })
const isDevelopment = process.env.NODE_ENV !== 'production'

module.exports = {
    webpack: (config, { dev, vendor }) => {
        const src = "app"
        const entries = [
            resolve(src, "*.{js,mjs,jsx,ts,tsx}"),
            resolve(src, "?(scripts)/*.{js,mjs,jsx,ts,tsx}"),
        ]
        return ({
            ...config,
            entry: GlobEntriesPlugin.getEntries(entries),
            resolve: {
                ...config.resolve,
                extensions: [...config.resolve.extensions, ".ts"],
            },
            module: {
                ...config.module,
                rules: [...config.module.rules, {test: /\.ts?$/, loader: "ts-loader"}]
            },
            plugins: [
                ...config.plugins,
                new webpack.DefinePlugin({
                    'process.env': JSON.stringify(dotenv.parsed),
                    'process.env.NODE_ENV': JSON.stringify(isDevelopment ? 'development' : 'production'),
                }),
            ]
        })
    },
}