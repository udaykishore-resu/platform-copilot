#!/usr/bin/env bash
# Assemble the MkDocs source tree in docs/ from the repo's canonical files.
# docs/ is generated (gitignored); README.md, modules/ and adr/ stay the source of truth.
set -euo pipefail
cd "$(dirname "$0")/.."
rm -rf docs && mkdir -p docs/modules docs/adr
cp README.md docs/index.md
cp ARCHITECTURE.md CONTRIBUTING.md docs/
cp -R modules/. docs/modules/

cp -R adr/. docs/adr/
# make relative links that point at repo files resolve on the site
sed -i.bak -E 's#\]\((modules/[^)]+)\)#](\1)#g; s#\(LICENSE\)#(https://github.com/udaykishore-resu/platform-copilot/blob/main/LICENSE)#g; s#\(go\.mod\)#(https://github.com/udaykishore-resu/platform-copilot/blob/main/go.mod)#g' docs/index.md
rm -f docs/index.md.bak
echo "docs/ assembled"
