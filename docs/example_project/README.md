# Example project workflow

`example_project/github/workflows/example-project.yml` runs four jobs in order: test the Go app and deployment examples, build and publish its container image, deploy the test environment, then deploy production. Copy the workflow to `.github/workflows/` in the example project repository, then run it with **Actions → Example project pipeline → Run workflow**. The deploy jobs use self-hosted runners so they can reach the lab controller API.

## GitHub configuration

Create GitHub environments named `test` and `prod`, and define the organisation variable `LAB_CONTROLLER_API_URL` as the controller API base URL, such as `http://10.10.10.2:8080`. The node names are specified directly in each environment's `.forearm` file and must match the API host's `nodes.yaml`. The self-hosted runners need network access to the API and `curl` and `jq`. Configure required reviewers on the `prod` environment if production deployment needs approval.

The workflow publishes `ghcr.io/<repository-owner>/example-project:<commit-sha>`. Make the GHCR package public so workload hosts can pull it; the deployment API does not configure registry credentials on those hosts. Deployments use the same commit image in test and production. Environment-specific forearm and JSON files supply the stack/database configuration and greeting.
