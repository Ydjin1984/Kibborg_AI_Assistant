package main

import "testing"

func TestClassifyMedia(t *testing.T) {
	cases := []struct {
		name, mime string
		want       MediaKind
	}{
		{"", "", mediaNone},
		{"shot.png", "image/png", mediaImage},
		{"photo.jpg", "", mediaImage},
		{"chart.WEBP", "application/octet-stream", mediaImage},
		{"voice.webm", "audio/webm", mediaAudio},
		{"note.ogg", "audio/ogg", mediaAudio},
		{"clip.mp4", "video/mp4", mediaVideo},
		{"clip.mp4", "", mediaVideo},
		{"movie.mov", "application/octet-stream", mediaVideo},
		{"main.go", "text/plain", mediaTextFile},
		{"README.md", "", mediaTextFile},
		{"data.bin", "application/octet-stream", mediaOther},
		// mime wins over extension
		{"weird.bin", "image/jpeg", mediaImage},
		{"x.webm", "video/webm", mediaVideo},
	}
	for _, c := range cases {
		got := classifyMedia(c.name, c.mime)
		if got != c.want {
			t.Errorf("classifyMedia(%q,%q)=%s want %s", c.name, c.mime, got, c.want)
		}
	}
}

func TestIsImageUploadDelegates(t *testing.T) {
	if !isImageUpload("a.png", "") {
		t.Fatal("png should be image")
	}
	if isImageUpload("a.mp4", "") {
		t.Fatal("mp4 must not be image")
	}
	if isAudioUpload("a.png", "image/png") {
		t.Fatal("png must not be audio")
	}
}
