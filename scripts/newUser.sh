#this creates a new user admin  with a given password
# to change an existing passowrd: run with reset-password, not create
read -r -s -p "New administrator password: " PRONOGTI_ADMIN_PASSWORD
printf '\n'
export PRONOGTI_ADMIN_PASSWORD

go run ./cmd/admin -config ./config.json reset-password \
	-name admin \
	-password-env PRONOGTI_ADMIN_PASSWORD

unset PRONOGTI_ADMIN_PASSWORD
