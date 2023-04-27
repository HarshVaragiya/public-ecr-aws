pipeline {
    agent any
    stages {
        stage("login"){
            steps {
                sh "aws ecr get-login-password --region ap-south-2 | docker login --username AWS --password-stdin ${REPOSITORY}"
            }
        }
        stage("build"){
            steps {
                sh "docker build -t ${REPOSITORY}/public-repo-scanner:${IMAGE_TAG} ."
            }
        }
        stage("push") {
            steps{
                sh "docker push ${REPOSITORY}/public-repo-scanner:${IMAGE_TAG}"
            }
        }
        stage("cleanup"){
            steps {
                build('docker-cleanup')
            }
        }
    }
}