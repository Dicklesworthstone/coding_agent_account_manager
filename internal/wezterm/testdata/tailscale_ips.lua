-- WezTerm config with Tailscale IPs
local wezterm = require 'wezterm'
local config = {}

-- Using Tailscale 100.x.x.x addresses directly
config.ssh_domains = {
  {
    name = 'ts-server1',
    remote_address = '100.90.148.85',
    username = 'ubuntu',
    multiplexing = 'WezTerm',
    ssh_option = {
      identityfile = wezterm.home_dir .. '/.ssh/ts.pem',
    },
  },
  {
    name = 'ts-server2',
    remote_address = '100.100.118.85',
    username = 'ubuntu',
    multiplexing = 'WezTerm',
  },
  {
    name = 'public-server',
    remote_address = '203.0.113.50',
    username = 'admin',
    multiplexing = 'WezTerm',
  },
}

return config
