-- Shipped with stremio-cliuwu and loaded into the mpv it starts.
--
-- Alt+j opens one subtitle menu: Off, the tracks mpv already has, and the
-- subtitles your addons offer that aren't loaded yet. The app does the fetching
-- and loading of addon subtitles; this script draws the list, switches tracks
-- itself, and reports an addon pick back. The binding is forced so it replaces
-- mpv's built-in Alt+j picker here, while a plain mpv keeps that one. Rebind
-- from input.conf with:  KEY script-binding stremio_subs/stremio-subs

local mp = require "mp"
local utils = require "mp.utils"
-- mp.input arrived in mpv 0.38. Without it the key says so instead of silently
-- doing nothing.
local has_input, input = pcall(require, "mp.input")
-- Set by the app when it starts mpv. Without it there is nobody to ask for
-- addon subtitles, so the menu opens at once with the tracks alone.
local in_app = mp.get_opt("stremio_cliuwu") ~= nil

local menu_id = nil

local function track_label(t)
    local parts = { t.lang or "und" }
    if t.title and t.title ~= "" then parts[#parts + 1] = t.title end
    parts[#parts + 1] = t.external and "external" or "in file"
    return (t.selected and "● " or "  ") .. table.concat(parts, " · ")
end

local function act(row)
    if row.kind == "off" then
        mp.set_property("sid", "no")
    elseif row.kind == "track" then
        mp.set_property("sid", tostring(row.id))
    elseif row.kind == "addon" and menu_id then
        mp.commandv("script-message", "stremio-cliuwu-subs-pick",
                    tostring(menu_id), tostring(row.idx))
    end
end

-- addon_rows: [{label, url, file}] from the app, in the app's order. Row i
-- here is row i there, which is how a pick is named on the way back.
local function open_menu(addon_rows)
    local rows = { { kind = "off", label = "Off" } }
    local loaded, current = {}, 1
    for _, t in ipairs(mp.get_property_native("track-list", {})) do
        if t.type == "sub" then
            rows[#rows + 1] = { kind = "track", id = t.id, label = track_label(t) }
            if t.selected then current = #rows end
            if t.external and t["external-filename"] then
                loaded[t["external-filename"]] = true
            end
        end
    end
    -- A subtitle that is already a track (or whose synced copy is) isn't
    -- offered a second time.
    for i, r in ipairs(addon_rows or {}) do
        local have = loaded[r.url] or (r.file and r.file ~= "" and loaded[r.file])
        if not have then
            rows[#rows + 1] = { kind = "addon", idx = i, label = r.label }
        end
    end
    mp.set_property_native("user-data/stremio_subs/rows", rows)

    local items = {}
    for i, r in ipairs(rows) do items[i] = r.label end
    input.select({
        prompt = "Subtitles:",
        items = items,
        default_item = current,
        submit = function(i) act(rows[i]) end,
    })
end

mp.add_forced_key_binding("Alt+j", "stremio-subs", function()
    if not has_input then
        mp.osd_message("the subtitle menu needs mpv 0.38 or newer", 4)
        return
    end
    if not in_app then
        open_menu({})
        return
    end
    mp.osd_message("fetching subtitles…", 30)
    mp.commandv("script-message", "stremio-cliuwu-subs-list")
end)

-- The app answers with {"id": n, "rows": [{"label", "url", "file"}, …]}.
mp.register_script_message("menu", function(json)
    local data = utils.parse_json(json or "")
    mp.osd_message("", 0)
    if not data then return end
    menu_id = data.id
    open_menu(data.rows)
end)
