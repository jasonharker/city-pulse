// Jenkinsfile for city-pulse: test, build, scan, then point the cluster at the new image.
// Same shape as portfolio's. There is no staging: a green main build is deployed by
// rewriting the image line (marked `# city-pulse-image`) in both workloads in
// thales-kubernetes, which Flux then applies.

pipeline {
    agent {
        kubernetes {
            yaml """
apiVersion: v1
kind: Pod
metadata:
  labels:
    jenkins: agent
spec:
  serviceAccountName: jenkins
  containers:
  - name: golang
    image: golang:1.27.2-alpine3.24
    command:
    - sleep
    args:
    - 99d
  - name: kaniko
    image: gcr.io/kaniko-project/executor:debug
    command:
    - sleep
    args:
    - 99d
  - name: git
    image: alpine/git:2.54.0
    command:
    - sleep
    args:
    - 99d
  - name: trivy
    image: aquasec/trivy:0.74.0
    command:
    - sleep
    args:
    - 99d
"""
        }
    }

    environment {
        REGISTRY = '10.10.20.31:5000'
        IMAGE_NAME = 'city-pulse'
        MANIFEST_REPO = 'git@github.com:jasonharker/thales-kubernetes.git'
        MANIFEST_FILES = 'apps/city-pulse/serve.yaml apps/city-pulse/nightly.yaml'
    }

    stages {
        stage('Test') {
            steps {
                container('golang') {
                    // Database tests skip without CITYPULSE_TEST_DATABASE_URL; they run locally against tools/devdb.
                    sh 'CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...'
                }
            }
        }

        stage('Build Image') {
            when { branch 'main' }
            steps {
                container('kaniko') {
                    sh """
                        /kaniko/executor \\
                            --context=\$(pwd) \\
                            --dockerfile=Dockerfile \\
                            --destination=${REGISTRY}/${IMAGE_NAME}:${GIT_COMMIT.take(8)} \\
                            --insecure \\
                            --skip-tls-verify \\
                            --cache=true \\
                            --cache-repo=${REGISTRY}/${IMAGE_NAME}-cache
                    """
                }
            }
        }

        stage('Vulnerability Scan') {
            when { branch 'main' }
            steps {
                container('trivy') {
                    script {
                        def image = "${REGISTRY}/${IMAGE_NAME}:${GIT_COMMIT.take(8)}"
                        sh "trivy image --insecure --format json -o trivy-report.json ${image}"
                        archiveArtifacts artifacts: 'trivy-report.json', allowEmptyArchive: true
                        sh "trivy image --insecure --severity CRITICAL --exit-code 1 ${image}"
                    }
                }
            }
        }

        stage('Deploy') {
            when { branch 'main' }
            steps {
                container('git') {
                    withCredentials([sshUserPrivateKey(credentialsId: 'github-ssh-deploy-key', keyFileVariable: 'SSH_KEY')]) {
                        script {
                            def shortCommit = GIT_COMMIT.take(8)
                            sh """
                                git config --global user.email "jenkins@thales.local"
                                git config --global user.name "Jenkins CI"
                                mkdir -p ~/.ssh
                                cp ${SSH_KEY} ~/.ssh/id_ed25519
                                chmod 600 ~/.ssh/id_ed25519
                                ssh-keyscan github.com >> ~/.ssh/known_hosts

                                git clone ${MANIFEST_REPO} k8s-manifests
                                cd k8s-manifests
                                sed -i 's|image: .* # city-pulse-image\$|image: ${REGISTRY}/${IMAGE_NAME}:${shortCommit} # city-pulse-image|' ${MANIFEST_FILES}
                                git diff ${MANIFEST_FILES}
                                git add ${MANIFEST_FILES}
                                git commit -m "🚀 Deploy ${IMAGE_NAME}:${shortCommit} (build #${BUILD_NUMBER})" || echo "No changes to commit"
                                git push origin main
                            """
                        }
                    }
                }
            }
        }
    }

    post {
        success {
            echo "Built ${IMAGE_NAME}:${GIT_COMMIT.take(8)}. Watch: kubectl -n city-pulse rollout status deploy/citypulse"
        }
    }
}
