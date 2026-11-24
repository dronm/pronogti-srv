profile_id=4
pronogti_config=config.json

TOCHKA_JWT=$(
	python3 - "$profile_id" "$pronogti_config" <<'PY'
import base64
import json
import os
import re
import subprocess
import sys
from pathlib import Path

from cryptography.exceptions import InvalidTag
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

profile_id, config_path = sys.argv[1:]
row = subprocess.check_output(
	["psql", "-XAt", 
	"-U", "pronogti",
	"-d", "pronogti",
	"-v", "ON_ERROR_STOP=1", 
	"-v",
	f"profile_id={profile_id}"],
	input="""
	SELECT json_build_array(company_id, id, encode(jwt_ciphertext, 'hex'))
	FROM public.payment_profiles
	WHERE id = :'profile_id'::bigint AND jwt_ciphertext IS NOT NULL;
	""",
	text=True,
).strip()
if not row:
	sys.exit("No encrypted JWT found for this profile")

company_id, profile_id, ciphertext = json.loads(row)
payments = json.loads(Path(config_path).read_text(encoding="utf-8")).get("payments", {})
key_env = payments.get("jwt_key_env", "PRONOGTI_PAYMENT_JWT_KEY")
value = payments.get("jwt_key") or os.environ.get(key_env)
if not value:
	sys.exit(f"Encryption key unavailable. Check the config or load {key_env} in this shell.")

if re.fullmatch(r"[0-9a-fA-F]{64}", value):
	key = bytes.fromhex(value)
else:
	key = base64.b64decode(value.replace("\r", "").replace("\n", ""), validate=True)

data = bytes.fromhex(ciphertext)
if len(key) != 32 or len(data) < 29 or data[0] != 1:
	sys.exit("Invalid key or unsupported JWT ciphertext format")

aad = f"pronogti:tochka:{company_id}:{profile_id}".encode()
try:
	token = AESGCM(key).decrypt(data[1:13], data[13:], aad)
except InvalidTag:
	sys.exit("JWT decryption failed. Check that the key matches the one used by Pronogti.")

sys.stdout.write(token.decode("ascii"))
PY
) && export TOCHKA_JWT

#******************************
#then run
if [[ -z ${TOCHKA_JWT:-} ]]; then
	printf '%s\n' 'TOCHKA_JWT is empty. Run the JWT-loading commands in this shell.'
elif [[ -z ${TOCHKA_CUSTOMER_CODE:-} ]]; then
	printf '%s\n' 'Set TOCHKA_CUSTOMER_CODE to the customer_code from your payment profile.'
else
	printf 'Authorization: Bearer %s\n' "$TOCHKA_JWT" |
		curl --silent --show-error --fail-with-body --get \
			'https://enter.tochka.com/uapi/acquiring/v1.0/retailers' \
			--data-urlencode "customerCode=$TOCHKA_CUSTOMER_CODE" \
			--header @-
fi

#********************************
printf 'Current customerCode: <%s>\n' "$TOCHKA_CUSTOMER_CODE"
printf 'Authorization: Bearer %s\n' "$TOCHKA_JWT" |
	curl --silent --show-error --fail-with-body \
		--header @- \
		'https://enter.tochka.com/uapi/open-banking/v1.0/customers'
