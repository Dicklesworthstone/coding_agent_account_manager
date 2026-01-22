-- WezTerm config with many ssh_domains
local wezterm = require 'wezterm'
local config = {}

config.ssh_domains = {
  {
    name = 'prod-server-1',
    remote_address = '10.0.1.1',
    username = 'deploy',
    multiplexing = 'WezTerm',
    ssh_option = {
      identityfile = '/home/user/.ssh/prod.pem',
    },
  },
  {
    name = 'prod-server-2',
    remote_address = '10.0.1.2',
    username = 'deploy',
    multiplexing = 'None',
    ssh_option = {
      identityfile = '/home/user/.ssh/prod.pem',
    },
  },
  {
    name = 'dev',
    remote_address = '192.168.50.10',
    username = 'developer',
    multiplexing = 'WezTerm',
    ssh_option = {
      identityfile = '~/.ssh/dev_key',
    },
  },
  {
    name = 'staging',
    remote_address = 'staging.example.com',
    username = 'staging',
    multiplexing = 'WezTerm',
    ssh_option = {
      identityfile = wezterm.home_dir .. '/.ssh/staging.pem',
    },
  },
  {
    name = 'localhost-test',
    remote_address = '127.0.0.1',
    username = 'testuser',
    multiplexing = 'None',
  },
}

return config
