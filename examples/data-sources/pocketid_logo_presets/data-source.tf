# Icons in Pocket ID's icon library whose name, reference or tags match.
data "pocketid_logo_presets" "media" {
  search = "jellyfin"
}

output "icon_references" {
  value = [for p in data.pocketid_logo_presets.media.presets : p.reference]
}
