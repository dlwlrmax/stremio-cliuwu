package player

import (
	"stremio-cliuwu/internal/stremio"
)

// SubsQueryFrom builds the subtitle request for a play request, so the picker
// and the autoloader ask for the same exact file.
func SubsQueryFrom(req *PlayRequest) stremio.SubsQuery {
	return stremio.SubsQuery{
		MediaType: req.MediaType,
		VideoID:   req.VideoID,
		Hash:      req.VideoHash,
		Size:      req.VideoSize,
		Filename:  req.Filename,
	}
}
