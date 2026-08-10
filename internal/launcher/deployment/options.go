package deployment

type options struct {
	command        string
	home           string
	manifestSource string
	frontendDir    string
	foreground     bool
	desktop        *desktopAssetSwitch
}
