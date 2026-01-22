-- Basic WezTerm config with ssh_domains
local wezterm = require 'wezterm'
local config = {}

config.font = wezterm.font 'JetBrains Mono'
config.font_size = 14.0

config.ssh_domains = {
  {
    name = 'csd',
    remote_address = '192.168.1.100',
    username = 'ubuntu',
    multiplexing = 'WezTerm',
    ssh_option = {
      identityfile = wezterm.home_dir .. '/.ssh/csd.pem',
    },
  },
  {
    name = 'css',
    remote_address = '192.168.1.101',
    username = 'admin',
    multiplexing = 'WezTerm',
    ssh_option = {
      identityfile = wezterm.home_dir .. '/.ssh/css.pem',
    },
  },
}

return config
