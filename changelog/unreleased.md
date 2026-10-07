# Unreleased

## Changed

- The computer name syncs both ways between the app and the web console. Both show the name with an edit (✎) button beside it; clicking it opens an inline field with Save and Cancel (Enter saves, Escape cancels). In the app this is under Connection → Cloud account once the account is linked; the web console's separate Rename button and bottom form are gone. A rename made in the web console shows up in the app within one heartbeat and is kept across restarts. A rename made while the cloud is unreachable is saved locally and delivered by the next heartbeat. Needs the cloud service redeployed (new `/api/agent/rename` endpoint; heartbeats accept a `name`).
