package handler

import "fmt"

// validConfigTemplate is the farm-minimal example from the config v1 contract with one
// player-facing word left open, so flow tests can save configs that are valid but different.
const validConfigTemplate = `{"schemaVersion":1,"game_id":"b21d94e7-3c0a-4f68-8e15-a9d7c4f02b61",` +
	`"theme":{"mode":"light","fonts":{"heading":{"family":"Fredoka","source":"google"},"body":{"family":"Nunito","source":"google"}},` +
	`"frame":{"style":"none","width":1,"color":"#D9DBD5","corners":"round"},"buttons":{"style":"outline","case":"normal"},` +
	`"panel":{"opacity":0.9,"blur":true},"colors":{"brand":"#2E8B57","background":"#FFFFFF","surface":"#F2F3F1","text":"#000000","muted":"#5B5D56"},"textures":{}},` +
	`"layout":{"position":"bottom-left","startCollapsed":false},"friends":{"enabled":true},` +
	`"clan":{"enabled":false,"maxMembers":10,"tags":false,"descriptionMax":140,"badges":{"source":"developer","allowed":[]},` +
	`"screens":{"discover":{"enabled":false},"create":{"enabled":false},"home":{"enabled":false,"header":"compact","showStats":false},"members":{"enabled":false}}},` +
	`"chat":{"enabled":true,"layout":"bubbles","directMessages":true,"clanChat":false,"maxMessageLength":500,"slowModeSeconds":0,"profanityFilter":false,"historyLimit":50},` +
	`"texts":{"terms":{"clan":"%s","clans":"%ss"},"labels":{}}}`

// cfg returns a valid config; different words give different (but equally valid) configs.
func cfg(word string) string { return fmt.Sprintf(validConfigTemplate, word, word) }
