# Artifacts are imported by "group_id/artifact_id"; the group id cannot
# contain a slash, the artifact id may. The import reads the metadata and
# the latest version, so a configuration with the same content shows no diff.
terraform import apicurio_artifact.price payments/price
