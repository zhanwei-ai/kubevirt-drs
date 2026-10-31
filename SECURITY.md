# Security policy

kubevirt-drs creates `VirtualMachineInstanceMigration` objects and patches VMI annotations. A bug can move
virtual machines; it cannot stop, delete or restart them (there is no eviction or delete code path). Treat
anything that could make it migrate the wrong VM, migrate in a loop, or exceed the migration limits read from
the KubeVirt CR as a security issue.

**Please do not open public issues for vulnerabilities.** Use GitHub's private vulnerability reporting on this
repository ("Security" tab, "Report a vulnerability"). If that form is not offered, contact a maintainer listed
in `.github/CODEOWNERS` directly. You will get an acknowledgement within 5 working days.

Supported versions: the latest minor release. Images are signed with cosign (keyless, GitHub OIDC) and ship a
CycloneDX SBOM; verify with `cosign verify ghcr.io/zhanwei-ai/kubevirt-drs:<tag> --certificate-identity-regexp
'https://github.com/zhanwei-ai/kubevirt-drs/' --certificate-oidc-issuer https://token.actions.githubusercontent.com`.
