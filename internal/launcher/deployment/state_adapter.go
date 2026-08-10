package deployment

import statepkg "athena-launcher/internal/state"

type launcherState = statepkg.State

func loadState(home string) (*launcherState, error) {
	return statepkg.Load(home)
}

func saveState(home string, value *launcherState) error {
	return statepkg.Save(home, value)
}
