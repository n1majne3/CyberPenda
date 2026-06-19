package runtimeextension_test

import (
	"testing"

	"pentest/internal/runtimeextension"
)

func TestParsePiPackageCatalogReadsExtensionCards(t *testing.T) {
	body := `<article class="surface-panel content-card" data-package-card="true" data-package-name="@scope/pi-tools" data-package-types="extension">
<div><h3><a href="/packages/@scope/pi-tools">@scope/pi-tools</a></h3>
<p class="packages-desc">Web tools &amp; search for Pi</p></div></article>`

	items := runtimeextension.ParsePiPackageCatalog(body)
	if len(items) != 1 {
		t.Fatalf("expected one item, got %#v", items)
	}
	item := items[0]
	if item.Provider != "pi" || item.ID != "@scope/pi-tools" || item.InstallRef != "npm:@scope/pi-tools" {
		t.Fatalf("unexpected item: %#v", item)
	}
	if item.Description != "Web tools & search for Pi" {
		t.Fatalf("unexpected description %q", item.Description)
	}
}
