"""Include the one versioned schema source in direct and sdist wheel builds."""

from pathlib import Path

from hatchling.builders.hooks.plugin.interface import BuildHookInterface


class CustomBuildHook(BuildHookInterface):
    def initialize(self, version: str, build_data: dict) -> None:
        root = Path(self.root)
        # An sdist has the generated source snapshot inside its own root.
        # A direct checkout wheel reads the authoritative repository directory.
        bundled = root / "contracts" / "jsonschema"
        checkout = root.parent / "contracts" / "jsonschema"
        if bundled.is_dir() and checkout.is_dir():
            raise RuntimeError("ambiguous training schema sources")
        source = bundled if bundled.is_dir() else checkout
        required = (
            "training-dataset-manifest.v1.schema.json",
            "training-dataset-manifest.v2.schema.json",
            "recommend-engagement.columns.v1.json",
            "search-qrel-dataset-manifest.v1.schema.json",
            "search-qrel.columns.v1.json",
        )
        if not source.is_dir() or any(not (source / name).is_file() for name in required):
            raise FileNotFoundError("versioned training schemas are missing from the build source")
        build_data["force_include"][str(source)] = "sea_training/schemas"
