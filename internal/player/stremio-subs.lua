-- Shipped with stremio-cliuwu and loaded into the mpv it starts.
--
-- Alt+s asks the app for subtitles from your addons and shows them as a menu
-- inside mpv. The app does the fetching and loading; this script only draws
-- the list and reports the choice. Rebind from input.conf with:
--   KEY script-binding stremio_subs/stremio-subs

local mp = require "mp"
local utils = require "mp.utils"
-- mp.input arrived in mpv 0.38. Without it the key says so instead of silently
-- doing nothing.
local has_input, input = pcall(require, "mp.input")

mp.add_key_binding("Alt+s", "stremio-subs", function()
    if not has_input then
        mp.osd_message("the subtitle menu needs mpv 0.38 or newer", 4)
        return
    end
    mp.osd_message("fetching subtitles…", 30)
    mp.commandv("script-message", "stremio-cliuwu-subs-list")
end)

-- The app answers with {"id": n, "items": ["English · …", …]}.
mp.register_script_message("menu", function(json)
    local data = utils.parse_json(json or "")
    if not data or type(data.items) ~= "table" or #data.items == 0 then
        mp.osd_message("no subtitles found", 3)
        return
    end
    mp.osd_message("", 0)
    input.select({
        prompt = "Subtitles:",
        items = data.items,
        submit = function(index)
            mp.commandv("script-message", "stremio-cliuwu-subs-pick",
                        tostring(data.id), tostring(index))
        end,
    })
end)
