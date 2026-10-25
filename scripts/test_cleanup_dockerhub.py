import unittest

from cleanup_dockerhub import tags_to_delete


class TagRetentionTests(unittest.TestCase):
    def test_keeps_eight_newest_semver_tags_by_numeric_version(self):
        tags = [f"1.0.{patch}" for patch in range(1, 11)]

        self.assertEqual(tags_to_delete(tags), ["1.0.1", "1.0.2"])

    def test_protects_the_release_tag_pinned_by_deployments(self):
        tags = ["0.1.0"] + [f"1.0.{patch}" for patch in range(1, 11)]

        self.assertEqual(tags_to_delete(tags), ["1.0.1", "1.0.2"])

    def test_deletes_sha_tags_but_preserves_latest_aliases_and_custom_tags(self):
        tags = [
            "latest",
            "1",
            "1.2",
            "1.2.3",
            "sha-abcdef0",
            "dev",
            "candidate",
        ]

        self.assertEqual(tags_to_delete(tags), ["sha-abcdef0"])

    def test_preserves_every_release_when_fewer_than_eight_exist(self):
        self.assertEqual(tags_to_delete(["0.1.0", "0.1.1", "latest"]), [])

    def test_requires_a_positive_release_count(self):
        with self.assertRaises(ValueError):
            tags_to_delete(["1.0.0"], keep_releases=0)


if __name__ == "__main__":
    unittest.main()