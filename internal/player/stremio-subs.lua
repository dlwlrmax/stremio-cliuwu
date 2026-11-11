-- Shipped with stremio-cliuwu and loaded into the mpv it starts.
--
-- Alt+j opens one subtitle menu: Off, the tracks mpv already has, and the
-- subtitles your addons offer that aren't loaded yet. The app does the fetching
-- and loading of addon subtitles; this script draws the list, switches tracks
-- itself, and reports an addon pick back. The binding is forced so it replaces
-- mpv's built-in Alt+j picker here, while a plain mpv keeps that one.
--
-- a opens the same kind of menu for the file's audio tracks. Switching there
-- is a plain aid change, which the app sees and remembers for the show; the
-- last row asks the app to forget that again.
--
-- Options (mpv.conf: script-opts-append=NAME=VALUE):
--   stremio_subs-key        the subtitle key, default Alt+j
--   stremio_subs-audio_key  the audio key, default a
--   stremio_subs-timeout    seconds to wait for the app's list, default 10

local mp = require "mp"
local utils = require "mp.utils"
-- mp.input arrived in mpv 0.38 and its select() in 0.39. Without it the key
-- says so instead of silently doing nothing.
local has_input, input = pcall(require, "mp.input")
has_input = has_input and input.select ~= nil
-- Set by the app when it starts mpv. Without it there is nobody to ask for
-- addon subtitles, so the menu opens at once with the tracks alone.
local in_app = mp.get_opt("stremio_cliuwu") ~= nil

local KEY = mp.get_opt("stremio_subs-key") or "Alt+j"
local AUDIO_KEY = mp.get_opt("stremio_subs-audio_key") or "a"
local TIMEOUT = tonumber(mp.get_opt("stremio_subs-timeout")) or 10

local menu_id = nil
-- The request being waited on. A reply for any other is ignored, so a late or
-- repeated answer can't pop a menu up under you.
local seq, pending = 0, nil

local function track_label(t)
    local parts = { t.lang or "und" }
    if t.title and t.title ~= "" then parts[#parts + 1] = t.title end
    parts[#parts + 1] = t.external and "external" or "in file"
    -- main-selection is 0 for the primary track and 1 for the secondary one;
    -- only the primary is "on" as far as this menu is concerned.
    local primary = t.selected and (t["main-selection"] == nil or t["main-selection"] == 0)
    return (primary and "● " or "  ") .. table.concat(parts, " · ")
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
            if t.selected and (t["main-selection"] == nil or t["main-selection"] == 0) then
                current = #rows
            end
            if t.external and t["external-filename"] then
                loaded[t["external-filename"]] = true
            end
        end
    end
    -- A subtitle that is already a track isn't offered a second time. Once a
    -- synced copy exists it is that copy that counts: the original being
    -- loaded must not hide the way to the retimed one.
    for i, r in ipairs(addon_rows or {}) do
        local have
        if r.file and r.file ~= "" then
            have = loaded[r.file]
        else
            have = loaded[r.url]
        end
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

mp.add_forced_key_binding(KEY, "stremio-subs", function()
    if not has_input then
        mp.osd_message("the subtitle menu needs mpv 0.39 or newer", 4)
        return
    end
    if not in_app then
        open_menu({})
        return
    end
    seq = seq + 1
    local mine = seq
    pending = mine
    mp.osd_message("fetching subtitles…", TIMEOUT + 2)
    mp.commandv("script-message", "stremio-cliuwu-subs-list", tostring(mine))
    -- If the app never answers (a slow addon, a dropped request) the tracks
    -- mpv has are still worth showing.
    mp.add_timeout(TIMEOUT, function()
        if pending == mine then
            pending = nil
            mp.osd_message("addons didn't answer — showing tracks only", 3)
            open_menu({})
        end
    end)
end)

-- The audio menu needs nothing from the app: every track is already in mpv.
local function audio_label(t)
    local parts = { t.lang or "und" }
    if t.title and t.title ~= "" then parts[#parts + 1] = t.title end
    if t["demux-channel-count"] then
        parts[#parts + 1] = tostring(t["demux-channel-count"]) .. "ch"
    end
    if t.codec then parts[#parts + 1] = t.codec end
    return (t.selected and "● " or "  ") .. table.concat(parts, " · ")
end

mp.add_forced_key_binding(AUDIO_KEY, "stremio-audio", function()
    if not has_input then
        mp.osd_message("the audio menu needs mpv 0.39 or newer", 4)
        return
    end
    local ids, items, current = {}, {}, 1
    for _, t in ipairs(mp.get_property_native("track-list", {})) do
        if t.type == "audio" then
            ids[#ids + 1] = t.id
            items[#items + 1] = audio_label(t)
            if t.selected then current = #items end
        end
    end
    if #ids == 0 then
        mp.osd_message("no audio tracks", 2)
        return
    end
    if in_app then
        items[#items + 1] = "  Forget this show's remembered audio"
    end
    input.select({
        prompt = "Audio:",
        items = items,
        default_item = current,
        submit = function(i)
            if ids[i] then
                mp.set_property("aid", tostring(ids[i]))
            else
                mp.commandv("script-message", "stremio-cliuwu-audio-forget")
            end
        end,
    })
end)

-- The app answers with {"req": "n", "id": n, "rows": [{"label","url","file"}, …]}.
mp.register_script_message("menu", function(json)
    local data = utils.parse_json(json or "")
    if not data or tostring(data.req) ~= tostring(pending) then return end
    pending = nil
    mp.osd_message("", 0)
    menu_id = data.id
    open_menu(data.rows)
end)
