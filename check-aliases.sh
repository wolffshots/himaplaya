#!/bin/sh
# Checks that each himalaya account maps the inbox, sent, drafts and trash
# roles to a mailbox that exists on the server. himalaya only shows a role
# on a mailbox when the alias of that role names it.
# ponytail: reads the JSON with grep and cut, so a mailbox name with a `"` or
# a `}` breaks it. Use jq if that happens.
accounts=$(himalaya --json account list | grep -o '"name":"[^"]*"' | cut -d'"' -f4)
if [ -z "$accounts" ]; then
	echo "FAIL  himalaya lists no accounts"
	exit 1
fi
fail=0
for account in $accounts; do
	echo "$account"
	if ! mailboxes=$(himalaya --json mailbox list --account "$account"); then
		echo "  FAIL  cannot list the mailboxes"
		fail=1
		continue
	fi
	missing=0
	for role in inbox sent drafts trash; do
		id=$(printf '%s' "$mailboxes" | tr '}' '\n' | grep "\"role\":\"$role\"" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
		if [ -n "$id" ]; then
			echo "  ok    $role -> $id"
		else
			echo "  FAIL  $role has no mailbox"
			missing=1
		fi
	done
	if [ $missing = 1 ]; then
		fail=1
		echo "  mailboxes on the server:"
		printf '%s' "$mailboxes" | grep -o '"id":"[^"]*"' | cut -d'"' -f4 | sed 's/^/    /'
	fi
done
exit $fail
