package main

import "github.com/rahulguptaio/timlee/internal/i18n"

var componentClientKeys = []i18n.Key{
	"components.alert.dismiss",
	"components.badge.remove",
	"components.code.code",
	"components.code.copied",
	"components.code.copiedCode",
	"components.code.copiedLabel",
	"components.code.copiedToClipboard",
	"components.code.copy",
	"components.code.copyCode",
	"components.code.copyFailed",
	"components.code.copyLabel",
	"components.code.scrollable",
	"components.dialog.close",
	"components.navigation.breadcrumb",
	"components.navigation.currentPage",
	"components.navigation.page",
	"components.navigation.pagination",
	"components.spinner.loading",
	"components.spinner.progress",
	"components.table.scrollable",
	"components.table.table",
	"components.toast.dismiss",
	"components.toast.notifications",
}

// joinClientKeys concatenates key groups for a page's client message block.
func joinClientKeys(groups ...[]i18n.Key) []i18n.Key {
	n := 0
	for _, group := range groups {
		n += len(group)
	}
	keys := make([]i18n.Key, 0, n)
	for _, group := range groups {
		keys = append(keys, group...)
	}
	return keys
}
