#!/usr/bin/env ruby

require 'json'
require 'date'
require 'open3'

pkg = 'spongix'
file = 'package.nix'

system 'go', 'mod', 'tidy'

puts 'Checking version...'

today = Date.today

version = `nix eval --raw '.##{pkg}.version'`.strip
md = version.match(/(?<y>\d+)\.(?<m>\d+)\.(?<d>\d+)\.(?<s>\d+)/)
version_date = Date.new(md[:y].to_i, md[:m].to_i, md[:d].to_i)
old_version = version

new_version =
  if today == version_date
    old_version.succ
  else
    today.strftime('%Y.%m.%d.001')
  end

if new_version != old_version
  puts "Updating version #{old_version} => #{new_version}"
  updated = File.read(file).gsub(old_version, new_version)
  File.write(file, updated)
else
  puts 'Skipping version update'
end

puts 'Checking vendorSha256...'

old_sha = `nix eval --raw '.##{pkg}.vendorSha256'`.strip
new_sha = nil

Open3.popen3('nix', 'build', ".##{pkg}.invalidHash") do |_si, _so, se|
  se.each_line do |line|
    puts line
    new_sha = $~[:sha] if line =~ /^\s+got:\s+(?<sha>sha256-\S+)$/
  end
end

pp old_sha, new_sha

if old_sha == new_sha
  puts 'Skipping vendorSha256 update'
else
  puts "Updating vendorSha256 #{old_sha} => #{new_sha}"
  updated = File.read(file).gsub(old_sha, new_sha)
  File.write(file, updated)
end
