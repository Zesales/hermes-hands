# Security policy

hermes-hands runs commands and edits files on the operator's machine at the
direction of a remote model, so its safety rails matter. See the README's
[Security model](README.md#security-model) for what they are.

## Supported versions

The latest release. Fixes ship as a new release; older ones are not patched.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private reporting: the
repository's **Security** tab → **Report a vulnerability**. You will get an
answer there.

In scope, for example:

- a way around the approval gate, the denylist or the repo jail
  (`local_read` / `local_write` / `local_edit` outside the launch directory)
- the API key or other secrets leaking — into results sent to hermes-agent,
  the session store, the debug trace, or the terminal
- the machine-bound secrets store (`secrets.enc`) decrypting where it should
  not
- anything that makes hermes-hands listen for inbound connections

Out of scope: what a command the operator approved goes on to do, and
vulnerabilities in hermes-agent itself — report those
[upstream](https://github.com/NousResearch/hermes-agent/security).
