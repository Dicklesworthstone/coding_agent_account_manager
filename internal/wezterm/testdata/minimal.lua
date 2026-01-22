-- Minimal WezTerm config with required fields only
local wezterm = require 'wezterm'
local config = {}

config.ssh_domains = {
  {
    name = 'minimal',
    remote_address = '10.0.0.1',
  },
}

return config
