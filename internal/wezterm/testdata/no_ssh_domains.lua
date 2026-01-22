-- WezTerm config without ssh_domains
local wezterm = require 'wezterm'
local config = {}

config.font = wezterm.font 'Fira Code'
config.font_size = 12.0
config.color_scheme = 'Dracula'

return config
